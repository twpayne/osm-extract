package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"runtime"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/geojson"
	"github.com/paulmach/osm"
	"github.com/paulmach/osm/osmpbf"
	"github.com/twpayne/go-geos"
	"golang.org/x/exp/slices"
)

var (
	compact        = flag.Bool("c", false, "compact output")
	inputFilename  = flag.String("i", "", "input filename (.osm.pbf format)")
	nodeID         = flag.Int("n", 0, "node ID")
	outputFilename = flag.String("o", "", "output filename")
	polygon        = flag.Bool("p", false, "way linestring as polygon") // FIXME improve help text
	procs          = flag.Int("j", runtime.GOMAXPROCS(0), "parallelism")
	relationID     = flag.Int("r", 0, "relation ID")
	wayID          = flag.Int("w", 0, "way ID")
)

func findNode(ctx context.Context, r io.ReadSeeker, nodeID osm.NodeID) (*osm.Node, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	// Scan to find the node.
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

	// Scan to find the way.
	wayScanner := osmpbf.New(ctx, r, *procs)
	defer wayScanner.Close()
	wayScanner.SkipNodes = true
	wayScanner.FilterWay = func(way *osm.Way) bool {
		return way.ID == wayID
	}
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

	// Find all nodes in the way.
	wayNodesByNodeID := make(map[osm.NodeID]*osm.WayNode, len(way.Nodes))
	for i, wayNode := range way.Nodes {
		wayNodesByNodeID[wayNode.ID] = &way.Nodes[i]
	}

	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	// Scan to find all nodes.
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

func findRelation(ctx context.Context, r io.ReadSeeker, relationID osm.RelationID) (*osm.Relation, map[string]orb.MultiLineString, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, err
	}

	// Find the relation.
	relationScanner := osmpbf.New(ctx, r, *procs)
	defer relationScanner.Close()
	relationScanner.SkipNodes = true
	relationScanner.SkipWays = true
	relationScanner.FilterRelation = func(relation *osm.Relation) bool {
		return relation.ID == relationID
	}
	var relation *osm.Relation
	for relationScanner.Scan() {
		var ok bool
		relation, ok = relationScanner.Object().(*osm.Relation)
		if ok {
			break
		}
	}
	if err := relationScanner.Err(); err != nil {
		return nil, nil, err
	}
	if err := relationScanner.Close(); err != nil {
		return nil, nil, err
	}
	if relation == nil {
		return nil, nil, nil
	}

	// Find all way IDs in the relation.
	wayIDs := make(map[osm.WayID]struct{}, len(relation.Members))
	for _, member := range relation.Members {
		if member.Type != "way" {
			continue
		}
		wayID := osm.WayID(member.Ref)
		wayIDs[wayID] = struct{}{}
	}

	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, err
	}

	// Find all waysByWayID amd node IDs.
	waysByWayID := make(map[osm.WayID]*osm.Way, len(wayIDs))
	wayNodeIDs := make(map[osm.NodeID]struct{})
	wayScanner := osmpbf.New(ctx, r, *procs)
	wayScanner.SkipNodes = true
	wayScanner.FilterWay = func(way *osm.Way) bool {
		_, ok := wayIDs[way.ID]
		return ok
	}
	wayScanner.SkipRelations = true
	for wayScanner.Scan() {
		if way, ok := wayScanner.Object().(*osm.Way); ok {
			waysByWayID[way.ID] = way
			for _, wayNode := range way.Nodes {
				wayNodeIDs[wayNode.ID] = struct{}{}
			}
		}
	}

	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, nil, err
	}

	// Scan to find all nodesByNodeID.
	nodesByNodeID := make(map[osm.NodeID]*osm.Node, len(wayNodeIDs))
	nodeScanner := osmpbf.New(ctx, r, *procs)
	nodeScanner.FilterNode = func(node *osm.Node) bool {
		_, ok := wayNodeIDs[node.ID]
		return ok
	}
	nodeScanner.SkipWays = true
	nodeScanner.SkipRelations = true
	for nodeScanner.Scan() {
		if node, ok := nodeScanner.Object().(*osm.Node); ok {
			nodesByNodeID[node.ID] = node
		}
	}
	if err := nodeScanner.Err(); err != nil {
		return nil, nil, err
	}
	if err := nodeScanner.Close(); err != nil {
		return nil, nil, err
	}

	// Construct MultiLineStrings.
	multiLineStringByRole := make(map[string]orb.MultiLineString)
	for _, member := range relation.Members {
		if member.Type != "way" {
			continue
		}
		wayID := osm.WayID(member.Ref)
		way, ok := waysByWayID[wayID]
		if !ok {
			log.Printf("relation %d: way %d: not found", relationID, wayID)
			continue
		}
		lineString := make(orb.LineString, 0, len(way.Nodes))
		for _, wayNode := range way.Nodes {
			node, ok := nodesByNodeID[wayNode.ID]
			if !ok {
				log.Printf("relation %d: way %d: node %d: not found", relationID, wayID, wayNode.ID)
				continue
			}
			point := orb.Point{node.Lon, node.Lat}
			lineString = append(lineString, point)
		}
		multiLineStringByRole[member.Role] = append(multiLineStringByRole[member.Role], lineString)
	}

	return relation, multiLineStringByRole, nil
}

func geosGeometry(geometry orb.Geometry) *geos.Geom {
	switch geometry := geometry.(type) {
	case orb.LineString:
		coords := make([][]float64, 0, len(geometry))
		for _, point := range geometry {
			coord := []float64{point.X(), point.Y()}
			coords = append(coords, coord)
		}
		return geos.NewLineString(coords)
	case orb.MultiLineString:
		geosLineStrings := make([]*geos.Geom, 0, len(geometry))
		for _, lineString := range geometry {
			geosLineString := geosGeometry(lineString)
			geosLineStrings = append(geosLineStrings, geosLineString)
		}
		return geos.NewCollection(geos.TypeIDGeometryCollection, geosLineStrings)
	default:
		panic(fmt.Sprintf("%s: unsupported type", geometry.GeoJSONType()))
	}
}

func orbGeometry(geosGeometry *geos.Geom) orb.Geometry {
	switch geosGeometry.TypeID() {
	case geos.TypeIDLinearRing:
		coords := geosGeometry.CoordSeq().ToCoords()
		ring := make(orb.Ring, 0, len(coords))
		for _, coord := range coords {
			point := orb.Point{coord[0], coord[1]}
			ring = append(ring, point)
		}
		return ring
	case geos.TypeIDPolygon:
		numInteriorRings := geosGeometry.NumInteriorRings()
		polygon := make(orb.Polygon, 0, 1+numInteriorRings)
		polygon = append(polygon, orbGeometry(geosGeometry.ExteriorRing()).(orb.Ring))
		return polygon
	default:
		panic(fmt.Sprintf("%s: unsupported type", geosGeometry.Type()))
	}
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
			feature.ID = node.FeatureID()
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
			feature.ID = way.FeatureID()
			appendTagProperties(feature.Properties, way.Tags)
			featureCollection.Append(feature)
		}
	}

	if *relationID != 0 {
		relation, multiLineStringByRole, err := findRelation(ctx, file, osm.RelationID(*relationID))
		if err != nil {
			return err
		}
		if relation != nil {
			// var geometry orb.Geometry
			if *polygon {
				outerMultiLineString := multiLineStringByRole["outer"]
				geom := geosGeometry(outerMultiLineString)
				geosOuterMultiPolygon := geos.PolygonizeValid([]*geos.Geom{geom})
				geometry := orbGeometry(geosOuterMultiPolygon)
				feature := geojson.NewFeature(geometry)
				feature.ID = relation.FeatureID()
				appendTagProperties(feature.Properties, relation.Tags)
				featureCollection.Append(feature)
			} else {
				for role, multiLineString := range multiLineStringByRole {
					feature := geojson.NewFeature(multiLineString)
					feature.ID = fmt.Sprintf("%d:%s", relation.FeatureID(), role)
					appendTagProperties(feature.Properties, relation.Tags)
					featureCollection.Append(feature)
				}
			}
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
