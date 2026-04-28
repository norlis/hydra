package cluster

import "errors"

type NodeReadinessChecker struct {
	discovery Discovery
}

func NewNodeReadinessChecker(d Discovery) *NodeReadinessChecker {
	return &NodeReadinessChecker{discovery: d}
}

func (c *NodeReadinessChecker) Check() error {
	if c.discovery == nil {
		return errors.New("cluster discovery is not initialized")
	}

	localNode := c.discovery.GetLocalNode()
	if !localNode.Healthy {
		return errors.New("local node topology reports unhealthy")
	}

	if len(localNode.Interfaces) == 0 {
		return errors.New("no proxy interfaces bound")
	}

	return nil
}
