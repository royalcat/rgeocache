BUILD_DIR="cache_build"

function download_maps {
    if [ $# -eq 0 ] || [ $# -gt 1 ]; then
        return
    fi

    MAPS=$1

    mkdir $BUILD_DIR
    pushd $BUILD_DIR
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

                curl --create-dirs -o "$file" "${zflag[@]}" "http://download.geofabrik.de/${file}"
            done
        popd


    popd
}

# MAPS_FILES=()
# export IFS=","
# for map in $MAPS; do
#     MAPS_FILES+="-i maps/${map}-latest.osm.pbf "
# done
# unset IFS
# go run ./cmd generate -p ${NAME}_points ${MAPS_FILES}