#!/bin/zsh
set -e

exec ssh -p 2222 -N \
  -D 127.0.0.1:1080 \
  -o ExitOnForwardFailure=yes \
  allevia@192.168.0.86
