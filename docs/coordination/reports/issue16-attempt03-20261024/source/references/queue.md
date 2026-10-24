<!-- 本文档由领域知识层结构化生成。仅「摘要/详述」可编辑；其余为系统维护，请勿修改。 -->

<!-- kg:uuid=85f91f1c-05be-5486-9594-2a3fe6f3fd3b tag=concept shared=false readonly-meta -->
## 有界队列阻塞规则
- **归属**：并发队列与重试控制 / 阻塞队列模型

**摘要**（可编辑）
有界队列在满时阻塞生产者、空时阻塞消费者的行为规则。

**详述**（可编辑）
有界队列阻塞规则指：当有界队列达到容量上限（本文为 8）时，生产者需要等待；当队列为空时，消费者需要等待。该规则解释了阻塞队列的基本含义，英文表述为 Bounded queue blocks when full or empty。
<!-- /kg:uuid=85f91f1c-05be-5486-9594-2a3fe6f3fd3b -->

<!-- kg:uuid=3456e85c-77bb-575e-9382-89408577ddde tag=entity shared=false readonly-meta -->
## 消费者
- **归属**：并发队列与重试控制 / 阻塞队列模型

**摘要**（可编辑）
从阻塞队列获取元素的角色，队列空时进入等待。

**详述**（可编辑）
消费者是从阻塞队列获取元素的角色。在容量为 8 的虚构队列中，当队列为空时，消费者不能继续获取而进入等待。
<!-- /kg:uuid=3456e85c-77bb-575e-9382-89408577ddde -->

<!-- kg:uuid=5a9ab1d8-f65d-504b-a743-33138f36c8b6 shared=true mirror=true source=_shared/并发队列与重试控制/producer.md -->
> 🔒 **[共享镜像 · 只读]** 本内容由 `_shared/并发队列与重试控制/producer.md` 维护，请勿在此编辑。
> **生产者** — 向阻塞队列提交元素的角色，队列满时进入等待。
<!-- /kg:uuid=5a9ab1d8-f65d-504b-a743-33138f36c8b6 -->

<!-- kg:uuid=c3123633-9fad-5391-8d39-ba4d7b393783 shared=true mirror=true source=_shared/并发队列与重试控制/retry-budget.md -->
> 🔒 **[共享镜像 · 只读]** 本内容由 `_shared/并发队列与重试控制/retry-budget.md` 维护，请勿在此编辑。
> **重试预算** — 生产者在不能无限等待时由调用方使用的重试预算机制，约束重试次数。
<!-- /kg:uuid=c3123633-9fad-5391-8d39-ba4d7b393783 -->

<!-- kg:uuid=87177315-8d2f-52d8-9d48-68fcf2afeb4e shared=true mirror=true source=_shared/并发队列与重试控制/blocking-queue.md -->
> 🔒 **[共享镜像 · 只读]** 本内容由 `_shared/并发队列与重试控制/blocking-queue.md` 维护，请勿在此编辑。
> **阻塞队列** — 具有固定容量（如容量为8）的队列组件，满时阻塞生产者、空时阻塞消费者。
<!-- /kg:uuid=87177315-8d2f-52d8-9d48-68fcf2afeb4e -->

