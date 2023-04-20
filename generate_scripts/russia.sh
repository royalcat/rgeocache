#!/bin/bash

#generator parameters
NAME="post-cis"
MAPS="russia"

mydir="${0%/*}"
source "$mydir"/generate.sh # generate_cache function import
generate_cache $NAME $MAPS