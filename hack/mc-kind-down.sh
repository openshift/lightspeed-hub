#!/usr/bin/env bash
set -euo pipefail

kind delete cluster --name lightspeed-hub    || true
kind delete cluster --name lightspeed-spoke1 || true
kind delete cluster --name lightspeed-spoke2 || true
echo "mc-kind-down: clusters deleted"
