# Durable notebook storage

The `notebooks` package separates ownership and revision metadata from file
bytes. `OpenPostgres` connects through a PostgreSQL DSN such as `DATABASE_URL`
and creates tables for notebooks and file metadata. `S3ObjectStore` uses the
AWS SDK credential chain and stores bytes in an S3-compatible bucket. Configure
`S3_BUCKET`, `AWS_REGION`, and optionally `S3_ENDPOINT` in deployment settings,
then pass them to `NewS3ObjectStore` as `S3Config`. Keep credentials in the
platform's environment/role configuration, not source control.

```go
objects, err := notebooks.NewS3ObjectStore(ctx, notebooks.S3Config{
    Bucket: os.Getenv("S3_BUCKET"),
    Region: os.Getenv("AWS_REGION"),
    Endpoint: os.Getenv("S3_ENDPOINT"), // empty for AWS S3
})
if err != nil { return err }
store, err := notebooks.OpenPostgres(ctx, os.Getenv("DATABASE_URL"), objects)
```

Objects are addressed by generated UUIDs below
`notebooks/<notebook-uuid>/<object-uuid>`, not by user-supplied paths. File
metadata records the object ID, byte size, content type, and committed revision.
A write uploads an immutable object first and then atomically updates the
PostgreSQL revision and metadata pointer. If the metadata transaction fails,
the uploaded object is not visible through notebook metadata; an orphaned
object may require later cleanup. Neither adapter stages durable content on
function-local disk.

This package is the persistence layer; Vercel HTTP routes and owner/revision
conflict policy are handled by the dependent notebook-operations task.
