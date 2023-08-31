#!/bin/bash
# Copyright 2023 The Go Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

set -e

# Deploy legacy database files.
gsutil -q -m cp -r /workspace/legacydb/* gs://go-vulndb

# Deploy v1 database files.
gsutil -m cp -r /workspace/db/* gs://go-vulndb

# Deploy web files.
# index.html is deployed as-is to avoid a name conflict with
# the "index/" folder, but other HTML files are deployed without the
# ".html" suffix for a cleaner URL.
gsutil cp webconfig/index.html gs://go-vulndb
for file in 404 copyright privacy; do
    gsutil -h "Content-Type:text/html" cp webconfig/$file.html gs://go-vulndb/$file
done
gsutil cp webconfig/favicon.ico gs://go-vulndb

