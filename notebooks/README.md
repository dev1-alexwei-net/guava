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

`internal/notebookops` is the repository-internal operation boundary for list,
read, write, and delete. It is an in-process Go package, not a public HTTP route;
no notebook operations are registered in `vercel.json`. Callers must pass the
owner ID obtained from trusted authentication, never an owner ID supplied by a
request. A notebook owned by a different identity is reported as not found.

Writes and deletes require the notebook's expected revision. Each successful
mutation advances the notebook revision by one; a stale revision returns
`notebooks.ErrConflict` (with expected and current values in `*ConflictError`)
and does not replace or remove the file metadata. `ErrNotFound` represents a
missing notebook/file or an ownership mismatch, while `ErrInvalidData` covers
invalid IDs, paths, and revisions. Listing and reading do not advance revisions.

Delete removes the file's metadata pointer, which is authoritative for reads
and listings. The immutable object is left unreferenced because PostgreSQL and
object storage cannot be updated atomically; object lifecycle cleanup is a
separate concern. As with a failed write metadata commit, cleanup may be needed
for unreferenced objects.
