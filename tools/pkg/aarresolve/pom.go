// Package aarresolve implements a small Maven-style dependency resolver for
// Android AAR/JAR closures. It fetches POMs from Google Maven and Maven
// Central, applies nearest-wins version resolution, and emits a deterministic
// JSON lock file plus a SHA-256 verified on-disk cache.
package aarresolve

import (
	"encoding/xml"
	"strings"
)

// POM is the subset of the Maven POM XML schema relevant to dependency
// resolution. Fields not consumed by the resolver are intentionally omitted.
type POM struct {
	XMLName    xml.Name      `xml:"project"`
	Parent     POMArtifact   `xml:"parent"`
	GroupID    string        `xml:"groupId"`
	ArtifactID string        `xml:"artifactId"`
	Version    string        `xml:"version"`
	Packaging  string        `xml:"packaging"`
	Properties POMProperties `xml:"properties"`
	DepMgmt    struct {
		Deps []POMDep `xml:"dependencies>dependency"`
	} `xml:"dependencyManagement"`
	Dependencies []POMDep `xml:"dependencies>dependency"`
}

// POMArtifact is a {group, artifact, version} triplet used for parent
// references.
type POMArtifact struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
	Version    string `xml:"version"`
}

// POMDep is a single <dependency> entry.
type POMDep struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
	Version    string `xml:"version"`
	Scope      string `xml:"scope"`
	Type       string `xml:"type"`
	Optional   string `xml:"optional"`
}

// POMProperties collects arbitrary <properties><k>v</k></properties> children
// into a map. Implements xml.Unmarshaler because the set of property names is
// open and cannot be expressed as struct fields.
type POMProperties struct {
	M map[string]string
}

// UnmarshalXML implements xml.Unmarshaler. It iterates child tokens of the
// <properties> element and records every <k>value</k> pair into M, stopping
// at the matching end element of start.
func (p *POMProperties) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	if p.M == nil {
		p.M = make(map[string]string)
	}
	var (
		curName string
		curVal  strings.Builder
		inChild bool
	)
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			curName = t.Name.Local
			curVal.Reset()
			inChild = true
		case xml.CharData:
			if inChild {
				curVal.Write(t)
			}
		case xml.EndElement:
			if t.Name == start.Name {
				return nil
			}
			if inChild && t.Name.Local == curName {
				p.M[curName] = strings.TrimSpace(curVal.String())
				inChild = false
			}
		}
	}
}
