package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/geojson"
	"github.com/paulmach/osm"
	"github.com/paulmach/osm/osmpbf"
	"golang.org/x/exp/slices"
)

var (
	compact        = flag.Bool("c", false, "compact output")
	inputFilename  = flag.String("i", "", "input filename (.osm.pbf format)")
	nodeID         = flag.Int("n", 0, "node ID")
	outputFilename = flag.String("o", "", "output filename")
	polygon        = flag.Bool("p", false, "way linestring as polygon") // FIXME improve help text
	procs          = flag.Int("j", runtime.GOMAXPROCS(0), "parallelism")
	wayID          = flag.Int("w", 0, "way ID")
)

func findNode(ctx context.Context, r io.ReadSeeker, nodeID osm.NodeID) (*osm.Node, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	scanner := osmpbf.New(ctx, r, *procs)
	defer scanner.Close()

	scanner.FilterNode = func(node *osm.Node) bool {
		return node.ID == nodeID
	}
	scanner.SkipRelations = true
	scanner.SkipWays = true
	for scanner.Scan() {
		if node, ok := scanner.Object().(*osm.Node); ok {
			return node, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return nil, nil
}

func findWay(ctx context.Context, r io.ReadSeeker, wayID osm.WayID) (*osm.Way, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	wayScanner := osmpbf.New(ctx, r, *procs)
	defer wayScanner.Close()
	wayScanner.FilterWay = func(way *osm.Way) bool {
		return way.ID == wayID
	}
	wayScanner.SkipNodes = true
	wayScanner.SkipRelations = true
	var way *osm.Way
	for wayScanner.Scan() {
		var ok bool
		if way, ok = wayScanner.Object().(*osm.Way); ok {
			break
		}
	}
	if err := wayScanner.Err(); err != nil {
		return nil, err
	}
	if err := wayScanner.Close(); err != nil {
		return nil, err
	}

	wayNodesByNodeID := make(map[osm.NodeID]*osm.WayNode, len(way.Nodes))
	for i, wayNode := range way.Nodes {
		wayNodesByNodeID[wayNode.ID] = &way.Nodes[i]
	}

	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	nodeScanner := osmpbf.New(ctx, r, *procs)
	defer nodeScanner.Close()
	nodeScanner.FilterNode = func(node *osm.Node) bool {
		return wayNodesByNodeID[node.ID] != nil
	}
	nodeScanner.SkipWays = true
	nodeScanner.SkipRelations = true
	for nodeScanner.Scan() {
		if node, ok := nodeScanner.Object().(*osm.Node); ok {
			wayNode := wayNodesByNodeID[node.ID]
			wayNode.Version = node.Version
			wayNode.ChangesetID = node.ChangesetID
			wayNode.Lat = node.Lat
			wayNode.Lon = node.Lon
		}
	}
	if err := nodeScanner.Err(); err != nil {
		return nil, err
	}
	if err := nodeScanner.Close(); err != nil {
		return nil, err
	}

	return way, nil
}

func appendTagProperties(properties geojson.Properties, tags osm.Tags) {
	for _, tag := range tags {
		properties[tag.Key] = tag.Value
	}
}

func run() error {
	ctx := context.Background()

	flag.Parse()

	file, err := os.Open(*inputFilename)
	if err != nil {
		return err
	}
	defer file.Close()

	featureCollection := geojson.NewFeatureCollection()

	if *nodeID != 0 {
		node, err := findNode(ctx, file, osm.NodeID(*nodeID))
		if err != nil {
			return err
		}
		if node != nil {
			feature := geojson.NewFeature(node.Point())
			feature.ID = node.ID
			appendTagProperties(feature.Properties, node.Tags)
			featureCollection.Append(feature)
		}
	}

	if *wayID != 0 {
		way, err := findWay(ctx, file, osm.WayID(*wayID))
		if err != nil {
			return err
		}
		if way != nil {
			var geometry orb.Geometry
			if *polygon {
				points := slices.Clone([]orb.Point(way.LineString()))
				if len(points) > 0 && points[len(points)-1] != points[0] {
					points = append(points, points[0])
				}
				geometry = orb.Polygon{orb.Ring(points)}
			} else {
				geometry = way.LineString()
			}
			feature := geojson.NewFeature(geometry)
			feature.ID = way.ID
			appendTagProperties(feature.Properties, way.Tags)
			featureCollection.Append(feature)
		}
	}

	file.Close()

	var writer io.Writer
	if *outputFilename == "" || *outputFilename == "-" {
		writer = os.Stdout
	} else {
		file, err := os.Create(*outputFilename)
		if err != nil {
			return err
		}
		defer file.Close()
		writer = file
	}

	jsonEncoder := json.NewEncoder(writer)
	if !*compact {
		jsonEncoder.SetIndent("", "\t")
	}
	if err := jsonEncoder.Encode(featureCollection); err != nil {
		return err
	}

	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
