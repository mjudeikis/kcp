# Limitations

* Gardener not always bumps generation, so if its not bumped, the runner will not pick up changes as it uses watches.
* Its not enough to delete, we need to add annotations to trigger delete.