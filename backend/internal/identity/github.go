package identity

// GitHubUser is what GitHub told us about a person at login. It carries no token: the
// OAuth access token is used for the login exchange and then discarded.
type GitHubUser struct {
	ID    int64
	Login string
	Name  string
	Email string
	// OrgMember is true when the person is an active member of the allowed organisation.
	OrgMember bool
	// TeamSlugs are the slugs of the GitHub teams (in the allowed organisation) the person
	// belongs to.
	TeamSlugs []string
}
