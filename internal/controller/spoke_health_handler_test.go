package controller_test

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	hubv1alpha1 "github.com/openshift/lightspeed-hub/api/v1alpha1"
	"github.com/openshift/lightspeed-hub/internal/controller"
	"github.com/openshift/lightspeed-hub/internal/credential"
)

var testKubeconfig = []byte(`apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://api.spoke.example.com:6443
    certificate-authority-data: ZmFrZS1jYQ==
  name: spoke
users:
- user:
    token: fake-token
  name: spoke-user
contexts:
- context:
    cluster: spoke
    user: spoke-user
  name: spoke
current-context: spoke
`)

func standingSecret(name, namespace string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      credential.StandingKubeconfigName(name),
			Namespace: namespace,
		},
		Data: map[string][]byte{
			credential.KubeconfigKey: testKubeconfig,
		},
	}
}

func spokeWithConnected(name string, status metav1.ConditionStatus) *hubv1alpha1.SpokeCluster {
	sc := newSpokeCluster(name)
	meta.SetStatusCondition(&sc.Status.Conditions, metav1.Condition{
		Type:   conditionTypeConnected,
		Status: status,
		Reason: "test",
	})
	return sc
}

var _ = Describe("SpokeHealthHandler", func() {
	const testNamespace = "test-operator-ns"

	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("should patch Connected=False when connected spoke becomes unreachable", func() {
		sc := spokeWithConnected("test-spoke", metav1.ConditionTrue)

		hubClient := fake.NewClientBuilder().
			WithScheme(newTestScheme()).
			WithObjects(sc, standingSecret(sc.Name, testNamespace)).
			WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
			Build()

		handler := controller.NewSpokeHealthHandler(hubClient, testNamespace, 5*time.Minute)
		handler.CheckConnectivity = func(cfg *rest.Config) error {
			return fmt.Errorf("connection refused")
		}

		handler.RunOnce(ctx)

		var updated hubv1alpha1.SpokeCluster
		Expect(hubClient.Get(ctx, types.NamespacedName{Name: sc.Name}, &updated)).To(Succeed())

		condition := meta.FindStatusCondition(updated.Status.Conditions, conditionTypeConnected)
		Expect(condition).NotTo(BeNil())
		Expect(condition.Status).To(Equal(metav1.ConditionFalse))
		Expect(condition.Reason).To(Equal(reasonConnectionFailed))
	})

	It("should patch Connected=True when disconnected spoke becomes reachable", func() {
		sc := spokeWithConnected("test-spoke", metav1.ConditionFalse)

		hubClient := fake.NewClientBuilder().
			WithScheme(newTestScheme()).
			WithObjects(sc, standingSecret(sc.Name, testNamespace)).
			WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
			Build()

		handler := controller.NewSpokeHealthHandler(hubClient, testNamespace, 5*time.Minute)
		handler.CheckConnectivity = func(cfg *rest.Config) error {
			return nil
		}

		handler.RunOnce(ctx)

		var updated hubv1alpha1.SpokeCluster
		Expect(hubClient.Get(ctx, types.NamespacedName{Name: sc.Name}, &updated)).To(Succeed())

		condition := meta.FindStatusCondition(updated.Status.Conditions, conditionTypeConnected)
		Expect(condition).NotTo(BeNil())
		Expect(condition.Status).To(Equal(metav1.ConditionTrue))
		Expect(condition.Reason).To(Equal(reasonConnectionSucceeded))
	})

	It("should not patch when connectivity status is unchanged", func() {
		sc := spokeWithConnected("test-spoke", metav1.ConditionTrue)

		hubClient := fake.NewClientBuilder().
			WithScheme(newTestScheme()).
			WithObjects(sc, standingSecret(sc.Name, testNamespace)).
			WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
			Build()

		var before hubv1alpha1.SpokeCluster
		Expect(hubClient.Get(ctx, types.NamespacedName{Name: sc.Name}, &before)).To(Succeed())

		handler := controller.NewSpokeHealthHandler(hubClient, testNamespace, 5*time.Minute)
		handler.CheckConnectivity = func(cfg *rest.Config) error {
			return nil
		}

		handler.RunOnce(ctx)

		var after hubv1alpha1.SpokeCluster
		Expect(hubClient.Get(ctx, types.NamespacedName{Name: sc.Name}, &after)).To(Succeed())
		Expect(after.ResourceVersion).To(Equal(before.ResourceVersion))
	})

	It("should skip spoke without standing kubeconfig", func() {
		sc := newSpokeCluster("test-spoke")

		hubClient := fake.NewClientBuilder().
			WithScheme(newTestScheme()).
			WithObjects(sc).
			WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
			Build()

		handler := controller.NewSpokeHealthHandler(hubClient, testNamespace, 5*time.Minute)
		handler.CheckConnectivity = func(cfg *rest.Config) error {
			Fail("CheckConnectivity should not be called for spoke without standing kubeconfig")
			return nil
		}

		handler.RunOnce(ctx)

		var updated hubv1alpha1.SpokeCluster
		Expect(hubClient.Get(ctx, types.NamespacedName{Name: sc.Name}, &updated)).To(Succeed())
		Expect(updated.Status.Conditions).To(BeEmpty())
	})

	It("should process all spokes even when one has a bad kubeconfig", func() {
		sc1 := spokeWithConnected("spoke-1", metav1.ConditionTrue)
		sc2 := spokeWithConnected("spoke-2", metav1.ConditionFalse)

		badSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      credential.StandingKubeconfigName("spoke-1"),
				Namespace: testNamespace,
			},
			Data: map[string][]byte{
				"wrong-key": []byte("bad"),
			},
		}

		hubClient := fake.NewClientBuilder().
			WithScheme(newTestScheme()).
			WithObjects(sc1, sc2, badSecret, standingSecret("spoke-2", testNamespace)).
			WithStatusSubresource(&hubv1alpha1.SpokeCluster{}).
			Build()

		handler := controller.NewSpokeHealthHandler(hubClient, testNamespace, 5*time.Minute)
		handler.CheckConnectivity = func(cfg *rest.Config) error {
			return nil
		}

		handler.RunOnce(ctx)

		// spoke-1: unchanged (bad Secret key, skipped)
		var u1 hubv1alpha1.SpokeCluster
		Expect(hubClient.Get(ctx, types.NamespacedName{Name: "spoke-1"}, &u1)).To(Succeed())
		c1 := meta.FindStatusCondition(u1.Status.Conditions, conditionTypeConnected)
		Expect(c1.Status).To(Equal(metav1.ConditionTrue))

		// spoke-2: patched to Connected=True
		var u2 hubv1alpha1.SpokeCluster
		Expect(hubClient.Get(ctx, types.NamespacedName{Name: "spoke-2"}, &u2)).To(Succeed())
		c2 := meta.FindStatusCondition(u2.Status.Conditions, conditionTypeConnected)
		Expect(c2.Status).To(Equal(metav1.ConditionTrue))
		Expect(c2.Reason).To(Equal(reasonConnectionSucceeded))
	})
})
