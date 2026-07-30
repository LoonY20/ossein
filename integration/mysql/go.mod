module github.com/LoonY20/ossein/integration/mysql

go 1.23.0

require (
	github.com/LoonY20/ossein v0.0.0
	github.com/go-sql-driver/mysql v1.9.3
)

require filippo.io/edwards25519 v1.1.0 // indirect

replace github.com/LoonY20/ossein => ../..
