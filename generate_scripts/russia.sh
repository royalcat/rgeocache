#!/bin/bash

#generator parameters
NAME="russia"
MAPS="russia"

mydir="${0%/*}"
source "$mydir"/generate.sh # generate_cache function import
download_maps $NAME $MAPS