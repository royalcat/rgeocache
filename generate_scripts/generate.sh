BUILD_DIR="cache_build"
RGEO_BIN="go run ../cmd"

function generate_cache {
    if [ $# -eq 0 ] || [ $# -gt 2 ]; then
        return
    fi

    NAME=$1
    MAPS=$2 

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

                curl --create-dirs -o "$file" "${zflag[@]}" "https://download.geofabrik.de/${file}"
            done
        popd

        MAPS_FILES=()
        export IFS=","
        for map in $MAPS; do
            MAPS_FILES+="-i maps/${map}-latest.osm.pbf "
        done
        unset IFS

        mydir="${0%/*}"
        $RGEO_BIN generate -p ${NAME}_points ${MAPS_FILES}
    popd
}