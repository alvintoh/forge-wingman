package runner

import (
	"context"
	"errors"
	"testing"
)

func TestFetchProjections(t *testing.T) {
	tests := []struct {
		name  string
		file  string
		fetch func(fakeObjects) (Projection, error)
	}{
		{"build", buildProjectionFile, func(o fakeObjects) (Projection, error) {
			return FetchBuildProjection(context.Background(), o, DefaultPointer)
		}},
		{"plan", planProjectionFile, func(o fakeObjects) (Projection, error) {
			return FetchPlanProjection(context.Background(), o, DefaultPointer)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objects := fakeObjects{
				DefaultPointer:                           []byte(testSHA + "\n"),
				"projections/" + testSHA + "/" + tt.file: []byte("rules " + ticketSentinel),
			}
			got, err := tt.fetch(objects)
			if err != nil {
				t.Fatal(err)
			}
			if got.SHA != testSHA || got.Text != "rules "+ticketSentinel {
				t.Fatalf("projection = %+v", got)
			}

			t.Run("missing pointer", func(t *testing.T) {
				var missing *MissingError
				if _, err := tt.fetch(fakeObjects{}); !errors.As(err, &missing) {
					t.Fatalf("err = %v, want MissingError", err)
				}
			})
			t.Run("pointer is not a sha", func(t *testing.T) {
				_, err := tt.fetch(fakeObjects{DefaultPointer: []byte("main")})
				if !errors.Is(err, ErrPointerInvalid) {
					t.Fatalf("err = %v, want ErrPointerInvalid", err)
				}
			})
			t.Run("missing projection", func(t *testing.T) {
				var missing *MissingError
				_, err := tt.fetch(fakeObjects{DefaultPointer: []byte(testSHA)})
				if !errors.As(err, &missing) {
					t.Fatalf("err = %v, want MissingError", err)
				}
			})
		})
	}
}
