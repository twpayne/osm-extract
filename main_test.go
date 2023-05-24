package main

import (
	"context"
	"os"
	"testing"

	"github.com/alecthomas/assert/v2"
	"github.com/paulmach/orb"
	"github.com/paulmach/osm"
)

func TestFindNodes(t *testing.T) {
	ctx := context.Background()

	file, err := os.Open("testdata/isle-of-man-latest.osm.pbf")
	assert.NoError(t, err)
	defer file.Close()

	nodeFilter, err := newNodeIDsFilter("286973603")
	assert.NoError(t, err)

	nodes, err := findNodes(ctx, file, nodeFilter)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(nodes))
	assert.Equal(t, osm.NodeID(286973603), nodes[0].ID)
	assert.Equal(t, 54.263872000000006, nodes[0].Lat)
	assert.Equal(t, -4.4610048, nodes[0].Lon)
}

func TestFindWays(t *testing.T) {
	ctx := context.Background()

	file, err := os.Open("testdata/isle-of-man-latest.osm.pbf")
	assert.NoError(t, err)
	defer file.Close()

	wayFilter, err := newWayIDsFilter("136226765")
	assert.NoError(t, err)

	ways, err := findWays(ctx, file, wayFilter)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(ways))
	assert.Equal(t, osm.WayID(136226765), ways[0].ID)
	assert.Equal(t, orb.LineString{
		{-4.461103400000001, 54.2636552},
		{-4.4609207, 54.263578200000005},
		{-4.4606671, 54.263783700000005},
		{-4.4607706, 54.263827400000004},
		{-4.4608498, 54.2638607},
		{-4.460959, 54.263772200000005},
		{-4.461103400000001, 54.2636552},
	}, ways[0].LineString())
}

func TestFindRelation(t *testing.T) {
	ctx := context.Background()

	file, err := os.Open("testdata/isle-of-man-latest.osm.pbf")
	assert.NoError(t, err)
	defer file.Close()

	relationFilter, err := newRelationIDsFilter("58446")
	assert.NoError(t, err)

	multiLineStringByRoleByRelation, err := findRelations(ctx, file, relationFilter)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(multiLineStringByRoleByRelation))
	for relation, multiLineStringByRole := range multiLineStringByRoleByRelation {
		assert.Equal(t, osm.RelationID(58446), relation.ID)
		assert.Equal(t, 3, len(multiLineStringByRole["outer"]))
	}
}
