# Test factories

Package `factory` builds deterministic application values without requiring an
ORM or reflection.

## Define a factory

```go
users := factory.New(func(sequence uint64) User {
	return User{
		Name:  fmt.Sprintf("User %d", sequence),
		Email: fmt.Sprintf("user%d@example.com", sequence),
		Role:  "member",
	}
})
```

Build one or many values:

```go
user := users.Build()
batch, err := users.BuildN(10)
```

The sequence is concurrency-safe and starts at 1. Use
`factory.NewSequence(start, builder)` when a different first value is useful.

## States

States customize defaults in order:

```go
admin := func(user *User) {
	user.Role = "admin"
}

user := users.Build(admin)
```

## Persistence

Factories do not know about a database. Pass an application-owned persistence
function when a test needs stored records:

```go
created, err := users.CreateN(ctx, 3, func(ctx context.Context, user User) error {
	_, err := db.ExecContext(
		ctx,
		`INSERT INTO users (name, email, role) VALUES ($1, $2, $3)`,
		user.Name,
		user.Email,
		user.Role,
	)
	return err
})
```

`CreateN` stops at the first persistence error and returns only successfully
stored values. For an all-or-nothing batch, call it inside
`database.WithinTransaction` and close the persister over the provided
`*sql.Tx`.
