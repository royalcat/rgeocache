#!/bin/bash

NAME="post-cis"
MAPS="europe/ukraine,russia,europe/belarus,europe/finland,europe/georgia,asia/afghanistan,asia/armenia,asia/azerbaijan,asia/kazakhstan,asia/uzbekistan,asia/mongolia"


echo "Downloading: ${MAPS}"
mkdir maps
pushd maps
export IFS=","
    for map in $MAPS; do
        file=${map}-latest.osm.pbf

        if test -e "$file"
        then zflag=(-z "$file")
        else zflag=()
        fi

        curl --create-dirs -o "$file" "${zflag[@]}" "https://download.geofabrik.de/${file}"
    done
popd

MAPS_FILES=()
export IFS=","
for map in $MAPS; do
    MAPS_FILES+="-i maps/${map}-latest.osm.pbf "
done
unset IFS
./main generate -p ${NAME}_points ${MAPS_FILES}
