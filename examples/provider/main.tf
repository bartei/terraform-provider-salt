terraform {
  required_providers {
    salt = {
      source  = "bartei/salt"
      version = "~> 0.1"
    }
  }
}

# Set a default Salt version for all resources
provider "salt" {
  salt_version = "3007"
}
