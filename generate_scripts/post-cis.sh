#!/bin/bash
MAPS="europe/ukraine,russia,europe/belarus,europe/finland,europe/georgia,asia/afghanistan,asia/armenia,asia/azerbaijan,asia/kazakhstan,asia/uzbekistan,asia/mongolia"


mydir="${0%/*}"
source "$mydir"/download_maps.sh # generate_cache function import
download_maps $MAPS
