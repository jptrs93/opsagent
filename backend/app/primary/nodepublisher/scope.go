package nodepublisher

type nodeScope struct {
	placements map[int]struct{}
	catalog    map[uint64]struct{}
	spaces     map[uint64]struct{}
}

func (rc *renderContext) scope(nodeID uint64) nodeScope {
	scope := nodeScope{
		placements: make(map[int]struct{}),
		catalog:    make(map[uint64]struct{}),
		spaces:     make(map[uint64]struct{}),
	}
	own := make(map[uint64]struct{})
	hostsGlobal := false
	for _, index := range rc.byNode[nodeID] {
		p := &rc.placements[index]
		scope.placements[index] = struct{}{}
		if p.spaceID == systemSpaceID {
			continue
		}
		own[p.spaceID] = struct{}{}
		scope.catalog[p.deploymentID] = struct{}{}
		if p.spaceID == globalSpaceID {
			hostsGlobal = true
		}
	}

	forward := make(map[uint64]struct{}, len(own))
	reverse := make(map[uint64]struct{}, len(own))
	for space := range own {
		forward[space] = struct{}{}
		reverse[space] = struct{}{}
		for to := range rc.out[space] {
			forward[to] = struct{}{}
		}
		for from := range rc.in[space] {
			reverse[from] = struct{}{}
		}
	}
	if len(own) > 0 {
		forward[globalSpaceID] = struct{}{}
	}
	delete(forward, systemSpaceID)
	delete(reverse, systemSpaceID)

	addSpace := func(space uint64, catalog bool) {
		for _, index := range rc.bySpace[space] {
			scope.placements[index] = struct{}{}
		}
		if !catalog {
			return
		}
		for i := range rc.services {
			if rc.services[i].SpaceID == space {
				scope.catalog[rc.services[i].DeploymentID] = struct{}{}
			}
		}
	}
	for space := range forward {
		scope.spaces[space] = struct{}{}
		addSpace(space, true)
	}
	for space := range reverse {
		scope.spaces[space] = struct{}{}
		addSpace(space, false)
	}
	if hostsGlobal {
		for space := range rc.bySpace {
			if space != systemSpaceID {
				addSpace(space, false)
			}
		}
	}

	for _, deploymentID := range rc.published[nodeID] {
		scope.catalog[deploymentID] = struct{}{}
		for _, index := range rc.byDeployment[deploymentID] {
			scope.placements[index] = struct{}{}
		}
	}
	for publisher, deployments := range rc.published {
		if publisher == nodeID {
			continue
		}
		reaches := false
		for _, deploymentID := range deployments {
			for _, index := range rc.byDeployment[deploymentID] {
				if rc.placements[index].nodeID == nodeID {
					reaches = true
					break
				}
			}
			if reaches {
				break
			}
		}
		if !reaches {
			continue
		}
		for _, index := range rc.byNode[publisher] {
			if rc.placements[index].netproxy {
				scope.placements[index] = struct{}{}
			}
		}
	}
	return scope
}
