# TfKloset

## Description

TfKloset is a proof of concept for a Terraform state server backed by [Kloset](https://github.com/PlakarKorp/kloset).
Since Terraform doesn't support custom state backends without modifying its source code, I decided to use the built-in HTTP backend.

**DISCLAIMER:** This is an experimental project, and IMHO, there is some real potential here. Kloset's properties could be particularly interesting
in highly regulated environments where state history, integrity, and traceability matter. By storing Terraform states as Kloset snapshots,
TfKloset benefits from Kloset's versioned storage model. This makes it possible to inspect previous states, retain historical
revisions, and replicate the underlying store using Plakar.

However, this is not intended for production use. The server handles sensitive information, including the credentials needed to access the Kloset store.
If compromised, an attacker could potentially gain access to your Terraform state files and the secrets they contain.

## Authentication

Kloset requires a secret to open the store, and I didn't want to store it in the server configuration.

According to the [Terraform documentation](https://developer.hashicorp.com/terraform/language/backend/http), the HTTP backend supports HTTP Basic authentication and mTLS.
While mTLS would be a nice option for authentication, it doesn't provide a straightforward way to pass the Kloset store password to the server.
As a workaround, the HTTP password contains both the user's password and the Kloset store passphrase.

Each value is independently Base64-encoded, and the two encoded values are separated by a colon (`:`).
Since Base64-encoded values cannot contain colons, the two passwords can be separated unambiguously, regardless of their original contents.

For example, if your user password is `deadbeef` and your Kloset store passphrase is `test`, your HTTP password should be:

`ZGVhZGJlZWY=:dGVzdA==`

**WARNING:** Base64 is encoding, not encryption. The credentials are still transmitted as part of HTTP Basic authentication.

User credentials are currently hard-coded in the source code, and the server does not support HTTPS. You've been warned.

## How it works

The idea is pretty simple:

- When Terraform sends a `GET /{name}` request, the server searches for snapshots matching the requested state name and returns the latest one.
- When Terraform sends a `POST /{name}` request with an updated state, the server creates a new Kloset snapshot.
- If the snapshot doesn't exist or if the state.json file doesn't exist in the snapshot, it returns a 404.

Each update therefore creates a new snapshot rather than overwriting the previous one, allowing you to browse the history of your Terraform state.

## How to run

**1. Create a Kloset store**

```bash
plakar at /tmp/kloset create
```

Enter and confirm your store passphrase.

**2. Configure the server**

Edit `tfkloset.yaml` to point to the store:

```yaml
name: TfKloset

server:
  address: 127.0.0.1
  port: 8080

store:
  path: /tmp/kloset
```

**3. Build and run**

```bash
make
./tfkloset
```

**4. Configure Terraform**

In your Terraform directory, create a `backend.tf` file:

```hcl
terraform {
  backend "http" {
    address        = "http://localhost:8080/test"
  }
}

resource "terraform_data" "test" {
  input = {
    message = "Hello TfKloset!"
    version = 1
  }
}

output "result" {
  value = terraform_data.test.output
}
```

**5. Configure authentication**

Set the following environment variables:

```bash
export TF_HTTP_USERNAME=admin
export TF_HTTP_PASSWORD="ZGVhZGJlZWY=:dGVzdA=="
```

**6. Create a new state**

```bash
terraform init
terraform apply
terraform state pull
```

Terraform will push the new state to TfKloset.

**7. Browse your state history**

You can now browse previous revisions of your Terraform state using the [Plakar](https://github.com/PlakarKorp/plakar) UI.
You can also use Plakar to synchronize the store with another Kloset store.

```bash
plakar at /tmp/kloset ui
```

## Limitations

TfKloset is an experimental project and currently has several limitations:

- **No state locking:** Concurrent Terraform operations are not prevented from modifying the same state.
- **No concurrency control:** State updates are not atomic, and concurrent writes may result in lost updates.
- **No state deletion:** The HTTP `DELETE` method is not implemented.
- **No HTTPS support:** Credentials are transmitted over unencrypted HTTP. The server should only be used in a trusted environment.
- **Hard-coded credentials:** User accounts and passwords are currently defined in the source code.
- **No automated recovery:** Previous state revisions can be browsed through Plakar, but restoring a specific revision is not currently exposed through TfKloset.
