# CRUD example

This example is a complete, dependency-free JSON API built with Ossein. It uses
an in-memory repository so the application flow remains easy to inspect.

It demonstrates:

- constructor-based dependency injection with an interface binding;
- route groups and named routes;
- JSON binding and explicit validation;
- structured HTTP errors;
- typed environment configuration;
- graceful shutdown;
- integration testing through `net/http`.

Run it:

```bash
go run ./examples/crud
```

Optionally change the address:

```bash
HTTP_ADDRESS=:9090 go run ./examples/crud
```

Routes:

```text
GET     /users
POST    /users
GET     /users/{id}
PUT     /users/{id}
DELETE  /users/{id}
```
