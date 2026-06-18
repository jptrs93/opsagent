package versionprovider

// Package-level instances, wired by the process bootstrap.
var (
	Git   *GitVersionProvider
	GHRel *GithubReleaseVersionProvider
)
