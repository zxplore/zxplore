#!/usr/bin/env sh
# zxplor installer — thin wrapper over the Makefile for a from-source install.
set -eu
exec make -C "$(dirname "$0")" install
