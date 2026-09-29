# PostGIS + pgvector in one image so dev, CI and prod run the same database.
# pgBackRest is in it too: production archives WAL with it for
# point-in-time recovery (deploy/backup/pitr.sh); dev never turns it on.
FROM postgis/postgis:17-3.5

RUN apt-get update \
 && apt-get install -y --no-install-recommends postgresql-17-pgvector pgbackrest \
 && rm -rf /var/lib/apt/lists/* \
 && install -d -o postgres -g postgres -m 750 /var/lib/pgbackrest /var/spool/pgbackrest /etc/pgbackrest

COPY deploy/pgbackrest/pgbackrest.conf /etc/pgbackrest/pgbackrest.conf
COPY deploy/docker/db-init/ /docker-entrypoint-initdb.d/
