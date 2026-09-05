#!/bin/sh
# DSH dynamically loads plugins relative to the current workspace. The coding
# workspace is a fresh tmpfs, so give Node ESM a temporary package-resolution
# path and remove it before the benchmark exports that workspace.
set -eu
created=0
if [ ! -e node_modules ]; then
  ln -s /opt/dsh/apps/cli/node_modules node_modules
  created=1
fi
cleanup() {
  if [ "$created" -eq 1 ]; then rm -f node_modules; fi
}
trap cleanup EXIT HUP INT TERM
/usr/local/bin/dsh-real "$@"
