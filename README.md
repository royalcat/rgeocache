# rgeocache

A reverse geocoding module with a pre-generated cache.

The cache is built based on OpenStreetMaps in pbf format,
you can download them from <http://download.geofabrik.de/>

## CLI Usage

The main usage scenarios are implemented in cmd/main.go as cli,
you can get a list of possible parameters by calling it without parameters.

Usage examples:

- ### Cache generation

```bash
go run cmd/main.go generate --input russia.osm.pbf --input ./europe/belarus.osm.pbf --points cis_points
```

where russia_points is the name of the cache file (will be saved with the .gob postfix)  
russia.osm.pbf and ./europe/belarus.osm.pbf are input files

Generating a cache of Russia will take about ~50GB of RAM. There is a possibility to shift the load from memory to disk by specifying the parameter --cache /tmp/rgeo_cache (you can specify any directory as the path), in this case, the generation process may significantly slow down

- ### HTTP Api

```bash
go run cmd/main.go serve --points cis_points
```

Starts an http server with a simple api for reverse geocoding based on the specified cache.  
The api documentation is described in the openapi format in the docs/api.yaml file  
An example of a simple request:

```bash
curl -X GET 'localhost:8080/rgeocode/address/59.9176846/30.3930866'
{"name":"","street":"Obvodny Canal embankment","house_number":"5 litA","city":"Saint Petersburg"}
```

## Usage as a go module

For go programs, you can avoid the http layer and use the geocoder directly using a module github.com/royalcat/rgeocache/geocoder

[![Go Reference](https://pkg.go.dev/badge/github.com/royalcat/rgeocache/geocoder.svg)](https://pkg.go.dev/github.com/royalcat/rgeocache/geocoder)

Example:

```go
geocache := &geocoder.RGeoCoder{}
err := geocache.LoadFromPointsFile("cis_points.gob")
loc, ok := rgeocoder.Find(lat, lon)
fmt.Printf("%s %s %s", loc.City, loc.Street, loc.HouseNumber)
```

## Convenience Scripts

In generate_scripts, there are scripts for automatic map downloading and cache generation.  
Scripts are divided by regions

- post-cis - countries of the post-Soviet space and former CIS
- russia
