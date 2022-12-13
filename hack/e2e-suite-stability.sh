#!/usr/bin/env bash

executions=0
success=0

for i in {1..100}
do
  ((executions++))
  if make e2e; then ((success++)); fi
  echo "executions $executions , success $success"
  make e2e-clean
done