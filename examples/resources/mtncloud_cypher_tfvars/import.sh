# By the entry's ID (shown in the console), or by its key, with or without the mount.
terraform import mtncloud_cypher_tfvars.tf_example_cypher_tfvars 1
terraform import mtncloud_cypher_tfvars.tf_example_cypher_tfvars tfvars/app/db
# A key made only of digits needs its mount, or it is read as an ID.
terraform import mtncloud_cypher_tfvars.tf_example_cypher_tfvars tfvars/2024
