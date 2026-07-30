package factory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
)

type user struct {
	Sequence uint64
	Email    string
	Role     string
}

func TestBuildAndStates(t *testing.T) {
	users := New(func(sequence uint64) user {
		return user{
			Sequence: sequence,
			Email:    fmt.Sprintf("user%d@example.com", sequence),
			Role:     "member",
		}
	})
	admin := func(value *user) { value.Role = "admin" }

	first := users.Build(admin)
	if first.Sequence != 1 || first.Email != "user1@example.com" || first.Role != "admin" {
		t.Fatalf("first user = %#v", first)
	}
	values, err := users.BuildN(2)
	if err != nil {
		t.Fatal(err)
	}
	if values[0].Sequence != 2 || values[1].Sequence != 3 {
		t.Fatalf("sequences = %#v", values)
	}
	if _, err := users.BuildN(-1); err == nil {
		t.Fatal("expected negative count error")
	}
}

func TestCreateAndPartialFailure(t *testing.T) {
	users := NewSequence(10, func(sequence uint64) user {
		return user{Sequence: sequence}
	})
	expected := errors.New("database failed")
	var persisted []uint64
	persist := func(_ context.Context, value user) error {
		if value.Sequence == 12 {
			return expected
		}
		persisted = append(persisted, value.Sequence)
		return nil
	}

	values, err := users.CreateN(nil, 4, persist)
	if !errors.Is(err, expected) {
		t.Fatalf("CreateN() error = %v", err)
	}
	if len(values) != 2 || values[0].Sequence != 10 || values[1].Sequence != 11 {
		t.Fatalf("created values = %#v", values)
	}
	if len(persisted) != 2 {
		t.Fatalf("persisted = %v", persisted)
	}
	if _, err := users.Create(context.Background(), nil); err == nil {
		t.Fatal("expected nil persister error")
	}
	if _, err := users.CreateN(context.Background(), -1, persist); err == nil {
		t.Fatal("expected negative count error")
	}
	if _, err := users.CreateN(context.Background(), 1, nil); err == nil {
		t.Fatal("expected nil persister error")
	}
}

func TestConcurrentBuildUsesUniqueSequence(t *testing.T) {
	values := New(func(sequence uint64) uint64 { return sequence })

	const count = 100
	sequences := make(chan uint64, count)
	var wait sync.WaitGroup
	for range count {
		wait.Add(1)
		go func() {
			defer wait.Done()
			sequences <- values.Build()
		}()
	}
	wait.Wait()
	close(sequences)

	result := make([]int, 0, count)
	for sequence := range sequences {
		result = append(result, int(sequence))
	}
	sort.Ints(result)
	for index, sequence := range result {
		if sequence != index+1 {
			t.Fatalf("sequence[%d] = %d", index, sequence)
		}
	}
}

func TestFactoryValidation(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected nil builder panic")
		}
	}()
	New[user](nil)
}
