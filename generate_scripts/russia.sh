#!/bin/bash

#generator parameters
MAPS="russia"

mydir="${0%/*}"
source "$mydir"/generate.sh # generate_cache function import
download_maps $MAPS