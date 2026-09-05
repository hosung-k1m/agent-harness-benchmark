#!/bin/sh
# Emits a regular tar stream. The trusted verifier performs all safety checks.
set -eu
workspace=$1
cd "$workspace"
tar --format=posix --numeric-owner --owner=0 --group=0 -cf - .
