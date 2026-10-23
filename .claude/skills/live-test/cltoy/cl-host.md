Review host: the toy CL host (`review.host = "command"`). A change is a CL, its URL `https://cl.toy/<repo>/<n>`; `cl` is on your PATH.
<!-- worker -->

- A CL: commit your work with git in your client, then run `cl upload` there: it sends the client's HEAD as your CL and prints its URL. Commit and `cl upload` again to revise it: one client holds one open CL.
<!-- queen -->

- Land a CL with `cl submit <url>` (run `{{bs}} check <worker>` first), once the keeper says to land it.
