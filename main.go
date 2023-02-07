package main

// FIXME implement Overpass API instead of -ids/-tags

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/geojson"
	"github.com/paulmach/osm"
	"github.com/paulmach/osm/osmpbf"
	"github.com/twpayne/go-geobabel"
	"github.com/twpayne/go-geos"
	"golang.org/x/exp/slices"
)

var (
	compact        = flag.Bool("compact", false, "compact output")
	idsFilterStr   = flag.String("ids", "", "ID filter")
	inputFilename  = flag.String("i", "", "input filename (.osm.pbf format)")
	osmType        = flag.String("type", "", "type (node, way, or relation)")
	outputFilename = flag.String("o", "", "output filename (GeoJSON format)")
	polygonize     = flag.Bool("polygonize", false, "polygonize ways")
	procs          = flag.Int("j", runtime.GOMAXPROCS(0), "parallelism")
	tagsFilterStr  = flag.String("tags", "", "tag filter")
)

func newNodeIDsFilter(idsFilter string) (func(*osm.Node) bool, error) {
	if idsFilter == "" {
		return nil, nil
	}
	nodeIDStrs := strings.Split(idsFilter, ",")
	nodeIDs := make(map[osm.NodeID]struct{}, len(nodeIDStrs))
	for _, nodeIDStr := range nodeIDStrs {
		nodeID, err := strconv.Atoi(nodeIDStr)
		if err != nil {
			return nil, err
		}
		nodeIDs[osm.NodeID(nodeID)] = struct{}{}
	}
	return func(node *osm.Node) bool {
		_, ok := nodeIDs[node.ID]
		return ok
	}, nil
}

func newWayIDsFilter(idsFilter string) (func(*osm.Way) bool, error) {
	if idsFilter == "" {
		return nil, nil
	}
	wayIDStrs := strings.Split(idsFilter, ",")
	wayIDs := make(map[osm.WayID]struct{}, len(wayIDStrs))
	for _, wayIDStr := range wayIDStrs {
		wayID, err := strconv.Atoi(wayIDStr)
		if err != nil {
			return nil, err
		}
		wayIDs[osm.WayID(wayID)] = struct{}{}
	}
	return func(way *osm.Way) bool {
		_, ok := wayIDs[way.ID]
		return ok
	}, nil
}

func newRelationIDsFilter(idsFilter string) (func(*osm.Relation) bool, error) {
	if idsFilter == "" {
		return nil, nil
	}
	relationIDStrs := strings.Split(idsFilter, ",")
	relationIDs := make(map[osm.RelationID]struct{}, len(relationIDStrs))
	for _, relationIDStr := range relationIDStrs {
		relationID, err := strconv.Atoi(relationIDStr)
		if err != nil {
			return nil, err
		}
		relationIDs[osm.RelationID(relationID)] = struct{}{}
	}
	return func(relation *osm.Relation) bool {
		_, ok := relationIDs[relation.ID]
		return ok
	}, nil
}

func newTagsFilter(tagsFilter string) (func(osm.Tags) bool, error) {
	if tagsFilter == "" {
		return nil, nil
	}
	requiredKeys := make(map[string]struct{})
	requiredValues := make(map[string]string)
	requiredRegexps := make(map[string]*regexp.Regexp)
	for _, pair := range strings.Split(tagsFilter, ",") {
		switch key, value, found := strings.Cut(pair, "="); {
		case !found:
			requiredKeys[key] = struct{}{}
		case len(value) >= 2 && value[0] == '/' && value[len(value)-1] == '/':
			requiredRegexp, err := regexp.Compile(value[1 : len(value)-1])
			if err != nil {
				return nil, err
			}
			requiredRegexps[key] = requiredRegexp
		default:
			requiredValues[key] = value
		}
	}
	return func(tags osm.Tags) bool {
		tagsMap := tags.Map()
		for requiredKey := range requiredKeys {
			if _, ok := tagsMap[requiredKey]; !ok {
				return false
			}
		}
		for requiredKey, requiredValue := range requiredValues {
			tagValue, ok := tagsMap[requiredKey]
			if !ok {
				return false
			}
			if tagValue != requiredValue {
				return false
			}
		}
		for requiredKey, requiredRegexp := range requiredRegexps {
			tagValue, ok := tagsMap[requiredKey]
			if !ok {
				return false
			}
			if !requiredRegexp.MatchString(tagValue) {
				return false
			}
		}
		return true
	}, nil
}

func findNodes(ctx context.Context, r io.ReadSeeker, filterNode func(*osm.Node) bool) ([]*osm.Node, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	// Scan to find nodes.
	var nodes []*osm.Node
	scanner := osmpbf.New(ctx, r, *procs)
	defer scanner.Close()
	scanner.FilterNode = filterNode
	scanner.SkipRelations = true
	scanner.SkipWays = true
	for scanner.Scan() {
		if node, ok := scanner.Object().(*osm.Node); ok {
			nodes = append(nodes, node)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return nodes, nil
}

func findWays(ctx context.Context, r io.ReadSeeker, filterWay func(*osm.Way) bool) ([]*osm.Way, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	// Scan to find ways.
	wayNodesIDs := make(map[osm.NodeID]struct{})
	wayScanner := osmpbf.New(ctx, r, *procs)
	defer wayScanner.Close()
	wayScanner.SkipNodes = true
	wayScanner.FilterWay = filterWay
	wayScanner.SkipRelations = true
	var ways []*osm.Way
	for wayScanner.Scan() {
		if way, ok := wayScanner.Object().(*osm.Way); ok {
			for _, wayNode := range way.Nodes {
				wayNodesIDs[wayNode.ID] = struct{}{}
			}
			ways = append(ways, way)
		}
	}
	if err := wayScanner.Err(); err != nil {
		return nil, err
	}
	if err := wayScanner.Close(); err != nil {
		return nil, err
	}

	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	// Scan to find all nodes.
	nodesByNodeID := make(map[osm.NodeID]*osm.Node, len(wayNodesIDs))
	nodeScanner := osmpbf.New(ctx, r, *procs)
	defer nodeScanner.Close()
	nodeScanner.FilterNode = func(node *osm.Node) bool {
		_, ok := wayNodesIDs[node.ID]
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
		return nil, err
	}
	if err := nodeScanner.Close(); err != nil {
		return nil, err
	}

	// Populate way nodes.
	for _, way := range ways {
		for i, wayNode := range way.Nodes {
			node := nodesByNodeID[wayNode.ID]
			way.Nodes[i].Lat = node.Lat
			way.Nodes[i].Lon = node.Lon
		}
	}

	return ways, nil
}

func findRelations(ctx context.Context, r io.ReadSeeker, relationFilter func(*osm.Relation) bool) (map[*osm.Relation]map[string]orb.MultiLineString, error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	// Scan to find relations.
	var relations []*osm.Relation
	wayIDs := make(map[osm.WayID]struct{})
	relationScanner := osmpbf.New(ctx, r, *procs)
	defer relationScanner.Close()
	relationScanner.SkipNodes = true
	relationScanner.SkipWays = true
	relationScanner.FilterRelation = relationFilter
	for relationScanner.Scan() {
		if relation, ok := relationScanner.Object().(*osm.Relation); ok {
			for _, member := range relation.Members {
				if member.Type != "way" {
					continue
				}
				wayID := osm.WayID(member.Ref)
				wayIDs[wayID] = struct{}{}
			}
			relations = append(relations, relation)
		}
	}
	if err := relationScanner.Err(); err != nil {
		return nil, err
	}
	if err := relationScanner.Close(); err != nil {
		return nil, err
	}

	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	// Find all node IDs.
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
		return nil, err
	}

	// Scan to find all nodes.
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
		return nil, err
	}
	if err := nodeScanner.Close(); err != nil {
		return nil, err
	}

	// Create MultiLineStrings.
	multiLineStringByRoleByRelation := make(map[*osm.Relation]map[string]orb.MultiLineString)
	for _, relation := range relations {
		multiLineStringByRole := make(map[string]orb.MultiLineString)
		for _, member := range relation.Members {
			if member.Type != "way" {
				continue
			}
			wayID := osm.WayID(member.Ref)
			way, ok := waysByWayID[wayID]
			if !ok {
				log.Printf("relation %d: way %d: not found", relation.ID, wayID)
				continue
			}
			lineString := make(orb.LineString, 0, len(way.Nodes))
			for _, wayNode := range way.Nodes {
				node, ok := nodesByNodeID[wayNode.ID]
				if !ok {
					log.Printf("relation %d: way %d: node %d: not found", relation.ID, wayID, wayNode.ID)
					continue
				}
				point := orb.Point{node.Lon, node.Lat}
				lineString = append(lineString, point)
			}
			multiLineStringByRole[member.Role] = append(multiLineStringByRole[member.Role], lineString)
		}
		multiLineStringByRoleByRelation[relation] = multiLineStringByRole
	}

	return multiLineStringByRoleByRelation, nil
}

func appendTagProperties(properties geojson.Properties, tags osm.Tags) {
	for _, tag := range tags {
		properties[tag.Key] = tag.Value
	}
}

func run() error {
	ctx := context.Background()

	flag.Parse()

	tagsFilter, err := newTagsFilter(*tagsFilterStr)
	if err != nil {
		return err
	}

	file, err := os.Open(*inputFilename)
	if err != nil {
		return err
	}
	defer file.Close()

	featureCollection := geojson.NewFeatureCollection()

	switch *osmType {
	case "node":
		nodeIDFilter, err := newNodeIDsFilter(*idsFilterStr)
		if err != nil {
			return err
		}
		var nodeFilter func(*osm.Node) bool
		switch {
		case nodeIDFilter != nil && tagsFilter == nil:
			nodeFilter = nodeIDFilter
		case nodeIDFilter == nil && tagsFilter != nil:
			nodeFilter = func(node *osm.Node) bool {
				return tagsFilter(node.Tags)
			}
		case nodeIDFilter != nil && tagsFilter != nil:
			nodeFilter = func(node *osm.Node) bool {
				return nodeIDFilter(node) && tagsFilter(node.Tags)
			}
		}
		nodes, err := findNodes(ctx, file, nodeFilter)
		if err != nil {
			return err
		}
		for _, node := range nodes {
			feature := geojson.NewFeature(node.Point())
			feature.ID = node.FeatureID()
			appendTagProperties(feature.Properties, node.Tags)
			featureCollection.Append(feature)
		}
	case "way":
		wayIDFilter, err := newWayIDsFilter(*idsFilterStr)
		if err != nil {
			return err
		}
		var wayFilter func(*osm.Way) bool
		switch {
		case wayIDFilter != nil && tagsFilter == nil:
			wayFilter = wayIDFilter
		case wayIDFilter == nil && tagsFilter != nil:
			wayFilter = func(way *osm.Way) bool {
				return tagsFilter(way.Tags)
			}
		case wayIDFilter != nil && tagsFilter != nil:
			wayFilter = func(way *osm.Way) bool {
				return wayIDFilter(way) && tagsFilter(way.Tags)
			}
		}
		ways, err := findWays(ctx, file, wayFilter)
		if err != nil {
			return err
		}
		for _, way := range ways {
			var geometry orb.Geometry
			if *polygonize {
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
	case "relation":
		relationIDFilter, err := newRelationIDsFilter(*idsFilterStr)
		if err != nil {
			return err
		}
		var relationFilter func(*osm.Relation) bool
		switch {
		case relationIDFilter != nil && tagsFilter == nil:
			relationFilter = relationIDFilter
		case relationIDFilter == nil && tagsFilter != nil:
			relationFilter = func(relation *osm.Relation) bool {
				return tagsFilter(relation.Tags)
			}
		case relationIDFilter != nil && tagsFilter != nil:
			relationFilter = func(relation *osm.Relation) bool {
				return relationIDFilter(relation) && tagsFilter(relation.Tags)
			}
		}
		multiLineStringByRoleByRelation, err := findRelations(ctx, file, relationFilter)
		if err != nil {
			return err
		}
		if *polygonize {
			geosContext := geos.NewContext()
			for relation, multiLineStringByRole := range multiLineStringByRoleByRelation {
				outerMultiLineString := multiLineStringByRole["outer"]
				geom := geobabel.NewGEOSGeomFromOrbGeometry(geosContext, outerMultiLineString)
				geosMultiPolygon := geosContext.PolygonizeValid([]*geos.Geom{geom})
				if innerMultiLineString, ok := multiLineStringByRole["inner"]; ok {
					geosInnerMultiLineString := geobabel.NewGEOSGeomFromOrbGeometry(geosContext, innerMultiLineString)
					geosInnerMultiPolygon := geosContext.PolygonizeValid([]*geos.Geom{geosInnerMultiLineString})
					geosMultiPolygon = geosMultiPolygon.Difference(geosInnerMultiPolygon)
				}
				geometry := geobabel.NewOrbGeometryFromGEOSGeom(geosMultiPolygon)
				feature := geojson.NewFeature(geometry)
				feature.ID = relation.FeatureID()
				appendTagProperties(feature.Properties, relation.Tags)
				featureCollection.Append(feature)
			}
		} else {
			for relation, multiLineStringByRole := range multiLineStringByRoleByRelation {
				for role, multiLineString := range multiLineStringByRole {
					feature := geojson.NewFeature(multiLineString)
					feature.ID = fmt.Sprintf("%d:%s", relation.FeatureID(), role)
					appendTagProperties(feature.Properties, relation.Tags)
					featureCollection.Append(feature)
				}
			}
		}
	default:
		return fmt.Errorf("%s: unknown type", *osmType)
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
