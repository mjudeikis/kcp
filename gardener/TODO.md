# Limitations

* Gardener not always bumps generation, so if its not bumped, the runner will not pick up changes as it uses watches.
* Its not enough to delete, we need to add annotations to trigger delete.


obj.SetManagedFields(nil) // server side apply does not want this