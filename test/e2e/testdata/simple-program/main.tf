variable "environment" {
  type    = string
  default = "test"
}

resource "random_pet" "name" {
  length = 2
}

output "pet_name" {
  value = random_pet.name.id
}

output "env" {
  value = var.environment
}
