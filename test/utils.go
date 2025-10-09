package test

import (
	"fmt"
	"io"
	"net/http"
	"os"
)

const (
	// Originally downloaded from http://download.geofabrik.de/europe/great-britain/england/greater-london.html
	londonFileName = "greater-london-140324.osm.pbf"
	londonFileURL  = "https://gist.githubusercontent.com/paulmach/853d57b83d408480d3b148b07954c110/raw/853f33f4dbe4246915134f1cde8edb30241ecc10/greater-london-140324.osm.pbf"

	// TODO replace with static file
	greatBritanName = "great-britain-latest.osm.pbf"
	greatBritanURL  = "https://download.geofabrik.de/europe/great-britain-latest.osm.pbf"
)

func downloadTestOSMFile(url, fileName string) error {
	if _, err := os.Stat(fileName); os.IsNotExist(err) {
		out, err := os.Create(fileName)
		if err != nil {
			return err
		}
		defer out.Close()

		resp, err := http.Get(url)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("test status code invalid: %v", resp.StatusCode)
		}

		if _, err := io.Copy(out, resp.Body); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	return nil
}
