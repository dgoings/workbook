package gitstore

// The tracking names of a project that shares its tasks through origin, which
// is every project these tests build unless one chooses another remote.
var (
	remoteTaskRefPrefix   = remoteTaskRefPrefixFor("origin")
	trackingTaskRefPrefix = remoteTaskRefPrefixFor("origin")
	remoteIdentityRef     = remoteIdentityRefFor("origin")
	remoteConfigRef       = remoteConfigRefFor("origin")
	identityFetchRefspec  = identityFetchRefspecFor("origin")
	configFetchRefspec    = configFetchRefspecFor("origin")
)
