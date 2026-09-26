# PostGIS + pgvector in one image so dev, CI and prod run the same database.
FROM postgis/postgis:17-3.5

RUN apt-get update \
 && apt-get install -y --no-install-recommends postgresql-17-pgvector \
 && rm -rf /var/lib/apt/lists/*

COPY deploy/docker/db-init/ /docker-entrypoint-initdb.d/
