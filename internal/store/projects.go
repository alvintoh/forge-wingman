package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// OwnerDatabase is the Firestore database the owner's controls live in — the
// allowlist here and each project's phase state — and the one the runner's IAM
// condition excludes (adr/0019).
const OwnerDatabase = "owner"

const projectsCollection = "projects"

// repositoryPattern is what GitHub calls a repository: an owner and a name.
var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)

// ErrRepositoryListed reports an add whose repository is already listed.
var ErrRepositoryListed = errors.New("repository already listed")

// ValidRepository reports whether repo is written as owner/name.
func ValidRepository(repo string) bool {
	return repositoryPattern.MatchString(repo)
}

// projectDoc is one listed project: the Linear project's name, its repository,
// and when the owner listed it.
type projectDoc struct {
	LinearProject string    `firestore:"linear_project"`
	Repository    string    `firestore:"repository"`
	AddedAt       time.Time `firestore:"added_at"`
}

// Projects keeps the owner's project allowlist in the owner database (adr/0019).
type Projects struct {
	client *firestore.Client
}

// NewProjects returns a Projects backed by client, which must be one opened on
// OwnerDatabase.
func NewProjects(client *firestore.Client) *Projects {
	return &Projects{client: client}
}

// projectDocID is the repository lowercased with its "/" replaced by "~": a
// document id cannot hold "/", and GitHub names a repository case-insensitively,
// so two spellings of one repository are one project.
func projectDocID(repository string) string {
	return strings.ReplaceAll(strings.ToLower(repository), "/", "~")
}

func (p *Projects) doc(repository string) *firestore.DocumentRef {
	return p.client.Collection(projectsCollection).Doc(projectDocID(repository))
}

// Add lists a project under its repository. It creates the document, so a
// repository already listed is ErrRepositoryListed and nothing is overwritten.
func (p *Projects) Add(ctx context.Context, linearProject, repository string, at time.Time) error {
	_, err := p.doc(repository).Create(ctx, projectDoc{
		LinearProject: linearProject,
		Repository:    repository,
		AddedAt:       at,
	})
	switch {
	case status.Code(err) == codes.AlreadyExists:
		return fmt.Errorf("%w: %s", ErrRepositoryListed, repository)
	case err != nil:
		return fmt.Errorf("adding %s: %w", repository, err)
	}
	return nil
}

// Remove unlists the project under repository, and reports false when no
// listed project names it — then nothing changes.
func (p *Projects) Remove(ctx context.Context, repository string) (bool, error) {
	doc := p.doc(repository)
	removed := false
	err := p.client.RunTransaction(ctx, func(_ context.Context, tx *firestore.Transaction) error {
		removed = false
		switch _, err := tx.Get(doc); {
		case status.Code(err) == codes.NotFound:
			return nil
		case err != nil:
			return fmt.Errorf("reading %s: %w", repository, err)
		}
		if err := tx.Delete(doc); err != nil {
			return fmt.Errorf("removing %s: %w", repository, err)
		}
		removed = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return removed, nil
}

// Repositories returns the repository of every listed project, sorted so the
// read is stable.
func (p *Projects) Repositories(ctx context.Context) ([]string, error) {
	docs, err := p.client.Collection(projectsCollection).Documents(ctx).GetAll()
	if err != nil {
		return nil, fmt.Errorf("reading the listed projects: %w", err)
	}
	repositories := make([]string, 0, len(docs))
	for _, doc := range docs {
		var listed projectDoc
		if err := doc.DataTo(&listed); err != nil {
			return nil, fmt.Errorf("decoding %s: %w", doc.Ref.Path, err)
		}
		repositories = append(repositories, listed.Repository)
	}
	slices.Sort(repositories)
	return repositories, nil
}
