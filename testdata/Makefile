.PHONY: all
all: isle-of-man-latest.osm.pbf

.PRECIOUS: %.osm.pbf
%.osm.pbf:
	curl -L -o $@ http://download.geofabrik.de/europe/$*.osm.pbf