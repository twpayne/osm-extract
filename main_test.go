package main

import (
	"context"
	"os"
	"testing"

	"github.com/paulmach/orb"
	"github.com/paulmach/osm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindNode(t *testing.T) {
	ctx := context.Background()

	file, err := os.Open("testdata/isle-of-man-latest.osm.pbf")
	require.NoError(t, err)
	defer file.Close()

	node, err := findNode(ctx, file, 286973603)
	require.NoError(t, err)
	assert.Equal(t, osm.NodeID(286973603), node.ID)
	assert.Equal(t, 54.263872000000006, node.Lat)
	assert.Equal(t, -4.4610048, node.Lon)
}

func TestFindWay(t *testing.T) {
	ctx := context.Background()

	file, err := os.Open("testdata/isle-of-man-latest.osm.pbf")
	require.NoError(t, err)
	defer file.Close()

	way, err := findWay(ctx, file, 136226765)
	require.NoError(t, err)
	assert.Equal(t, osm.WayID(136226765), way.ID)
	assert.Equal(t, orb.LineString{
		{-4.4609207, 54.263578200000005},
		{-4.4606671, 54.263783700000005},
		{-4.4607706, 54.263827400000004},
		{-4.4608498, 54.2638607},
		{-4.460959, 54.263772200000005},
		{-4.461103400000001, 54.2636552},
	}, way.LineString())
}

func TestFindRelation(t *testing.T) {
	ctx := context.Background()

	file, err := os.Open("testdata/isle-of-man-latest.osm.pbf")
	require.NoError(t, err)
	defer file.Close()

	relation, multiLineStringByRole, err := findRelation(ctx, file, 58446)
	require.NoError(t, err)
	assert.Equal(t, osm.RelationID(58446), relation.ID)
	assert.Len(t, multiLineStringByRole["outer"], 3)
}
