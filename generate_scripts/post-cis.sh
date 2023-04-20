#!/bin/bash
NAME="post-cis"
MAPS="europe/ukraine,russia,europe/belarus,europe/finland,europe/georgia,asia/afghanistan,asia/armenia,asia/azerbaijan,asia/kazakhstan,asia/uzbekistan,asia/mongolia"


mydir="${0%/*}"
source "$mydir"/generate.sh # generate_cache function import
generate_cache $NAME $MAPS