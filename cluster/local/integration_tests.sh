#!/usr/bin/env bash
set -e

# setting up colors
BLU='\033[0;34m'
YLW='\033[0;33m'
GRN='\033[0;32m'
RED='\033[0;31m'
NOC='\033[0m' # No Color

echo_success(){
    printf "\n${GRN}%s${NOC}\n" "$1"
}

echo_success "There are no integration tests in this project"