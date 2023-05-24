# osm-extract

osm-extract extracts features from
[OpenStreetMap](https://www.openstreetmap.org/) [PBF
files](https://wiki.openstreetmap.org/wiki/PBF_Format) as
[GeoJSON](https://geojson.org/).

## Features

* Reads `.osm.pbf` files from local disk, no need to run an [Overpass API
  server](https://wiki.openstreetmap.org/wiki/Overpass_API).
* Preserves OpenStreetMap tags as GeoJSON properties.
* Extremely fast, thanks to
  [`github.com/paulmach/osm`](https://github.com/paulmach/osm/).
* Optionally polygonizes ways and relations, thanks to [GEOS](https://libgeos.org).

## Install

```console
$ go install github.com/twpayne/osm-extract@latest
```

## Example

Extract the administrative boundaries of the Isle of Man as a polygon:

```console
$ osm-extract -i testdata/isle-of-man-latest.osm.pbf -type=relation -tags=ISO3166-1=IM,admin_level=2 -polygonize
```

## License

MIT