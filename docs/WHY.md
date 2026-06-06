# Built for AI Sysadmins

Context, I have worked in IT for several decades, managing infrastructure, managing applications, managing workflows.  Dealing with issues in large corporate environments, pursuing robust, resilient systems in complex ecosystems.  I have enjoyed that experience, and I'm good at it.
I'm a curious tinkerer, I like to build things, write software and learn through exploring.  I have turned on many automations in my home life, and most have been abandoned along the way for many different reasons, which can be summarised as 'technical entropy'. Over time things break, they unravel, disconnect, become out of date, incompatible and eventually just stop.
Ductile is my way of addressing some part of that.

I think it fair to say that there are very few projects on Github that anyone truly needs.  They might need something, and there are a lot of somethings.  For any slice of need there are a multitude of somethings, and only when we overlay our needs, requirements and preferences can we begin to short list and decide how to invest time learning a something.

Ductile might be that something for someone, and I will try and lay out some thinking dimensions, or areas of attention that might help filter Ductile in or out of a short list.

Ductile is designed to be operated by AI.  That's a first class requirement, that I set before even naming it, or making the repo folder.  Here's why:

One of the biggest frictions for me is the time and effort required to relearn the thing I built six months ago.  Regardless of whether it had apparent crystal clarity of design, or was a thrown together cobbling of ideas, after six months it's archeology.
After installing Claude Code on a few home systems, and just using it as an admin intern (grey-beard intern?), that friction is significantly reduced.  AI does really well figuring out stuff, with the right pointers, and steering it gets there.

My automations were a mix of shell scripts, Python, Go, cron, systemd, and webservers, mixed with tools and projects other people made.
Prior to creating Ductile, my last attempt to solve this was to write a personal API, everything would be represented in that, I thought.  It was ok, but I had issues with databases, across projects, tokens, scopes key sprawl, and an unplanned meandering endpoint schema.  What worked I didn't want to touch, and what broke, was not easier to fix.  The last straw for me was getting Claude to fix the issue, something got lost in translation and it refactored so much it just became far worse.  Besides that it didn't have a schedule, so I was back to systemd, and it didn't have any way to execute other projects' binaries.  So just not what I needed.

At that point I went back to looking for a lightweight, open source integration/workflow runner.  I looked at N8N and Node-Red and a few other job scheduling tools.  Nothing really appealed.
Looking back, honestly I think I wanted to build this, because once I started I was absolutely convinced it was the right thing to do.

Rules that make Ductile work:
1. Build it for AI, I'm not going to do the work.
2. Think about how an AI would choose an action.  Semantic anchoring, themes through the codebase, config, CLI and API.
3. Use YAML for config.  I like YAML and so does AI.
4. Have high standards at the Ductile end, the config, the way data is passed around, and more relaxed toward the execution tip of the plugins where I have less control.
5. Allow the plugin to be written in whatever language is useful, use a light but compliant wrapper as needed.
6. Generate useful logs that AI can use to explain what happened.  We have not solved Technology Entropy, so expect failures and plan for root cause analysis.

Ductile is definitely grown to be far more complex than I intended, I probably didn't need queues and events and to learn what idempotency is good for.  I guess that is not a unique characteristic for home brew code.  I wonder if the learning curve is now too high, and it's become the things I turned away from.
