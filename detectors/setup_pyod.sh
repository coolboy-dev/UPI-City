#!/usr/bin/env bash
# Install PyOD into a venv that borrows NumPy/SciPy/scikit-learn/numba from Nix.
#
# ─── Why not just `pip install pyod` ────────────────────────────────────────
#
# On NixOS pip's manylinux wheels cannot find libstdc++, so NumPy fails to
# import and every detector built on it looks broken rather than uninstalled.
# The fix is to let Nix supply everything that ships compiled code and let pip
# supply only the pure-Python layer on top: --system-site-packages exposes the
# Nix packages, and --no-deps stops pip from replacing them with wheels it
# cannot build.
set -euo pipefail

VENV="${1:-.venv-pyod}"

if [ ! -d "$VENV" ]; then
  echo "creating $VENV (borrowing numpy/scipy/scikit-learn/numba from Nix)"
  python3 -m venv --system-site-packages "$VENV"
fi

"$VENV/bin/pip" install -q --no-deps pyod
"$VENV/bin/python" - <<'PY'
import pyod
from pyod.models.iforest import IForest
from pyod.models.lof import LOF
from pyod.models.hbos import HBOS
from pyod.models.knn import KNN
from pyod.models.pca import PCA
from pyod.models.cblof import CBLOF
print(f"PyOD {pyod.__version__} ready — IForest LOF HBOS KNN PCA CBLOF")
PY
