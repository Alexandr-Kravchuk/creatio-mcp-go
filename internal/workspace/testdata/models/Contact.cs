#pragma warning disable CS8618, // Non-nullable field is uninitialized.

using ATF.Repository;
using ATF.Repository.Attributes;
using System;
using System.Collections.Generic;
using System.Diagnostics.CodeAnalysis;
using System.ComponentModel.DataAnnotations;

namespace Acme.Models {
	/// <summary>
	/// People the company works with
	/// </summary>
	[ExcludeFromCodeCoverage]
	[Schema("Contact")]
	public class Contact: BaseModel {

		#region Schema Columns

		/// <summary>
		/// Full name
		/// </summary>
		[Required(ErrorMessage = "The {0} field is required.")]
		[MinLength(1, ErrorMessage = "The {0} field must be a minimum of {1} character long.")]
		[MaxLength(250, ErrorMessage = "The {0} field must be a maximum of {1} character long.")]
		[SchemaProperty("Name")]
		public string Name { get; set; }

		/// <summary>
		/// Account
		/// </summary>
		[SchemaProperty("Account")]
		public Guid AccountId { get; set; }

		/// <inheritdoc cref="AccountId"/>
		[LookupProperty("Account")]
		public virtual Account Account { get; set; }

		/// <summary>
		/// Email
		/// </summary>
		[MaxLength(250, ErrorMessage = "The {0} field must be a maximum of {1} character long.")]
		[EmailAddress(ErrorMessage = "The {0} field is not a valid e-mail address.")]
		[SchemaProperty("Email")]
		public string Email { get; set; }

		/// <summary>
		/// Birth date
		/// </summary>
		[SchemaProperty("BirthDate")]
		public DateTime BirthDate { get; set; }

		/// <summary>
		/// Age
		/// </summary>
		[SchemaProperty("Age")]
		public int Age { get; set; }

		/// <summary>
		/// Is new
		/// </summary>
		[SchemaProperty("IsNew")]
		public new bool IsNew { get; set; }

		/// <summary>
		/// Photo data
		/// </summary>
		/// <summary>
		/// Owner
		/// </summary>
		[SchemaProperty("Owner")]
		public Guid OwnerId { get; set; }

		/// <inheritdoc cref="OwnerId"/>
		[LookupProperty("Owner")]
		public virtual Contact Owner { get; set; }

		/// <summary>
		/// Type
		/// </summary>
		[Required(ErrorMessage = "The {0} field is required.")]
		[SchemaProperty("Type")]
		public Guid TypeId { get; set; }

		/// <inheritdoc cref="TypeId"/>
		[LookupProperty("Type")]
		public virtual ContactType Type { get; set; }

		/// <summary>
		/// Weight
		/// </summary>
		[SchemaProperty("Weight")]
		public decimal Weight { get; set; }

		/// <summary>
		/// Notes about the contact.
Free text.
		/// </summary>
		[SchemaProperty("Notes")]
		public string Notes { get; set; }

		/// <summary>
		/// Created by
		/// </summary>
		[SchemaProperty("CreatedBy")]
		public Guid CreatedById { get; set; }

		/// <inheritdoc cref="CreatedById"/>
		[LookupProperty("CreatedBy")]
		public virtual Contact CreatedBy { get; set; }

		/// <summary>
		/// Gender
		/// </summary>
		[SchemaProperty("Gender")]
		public Guid Gender { get; set; }

		[SchemaProperty("Completeness")]
		public string Completeness { get; set; }

		#endregion

		#region Details

		/// <summary>
		/// Collection of Account by column OwnerId
		/// <remarks>
		/// <see cref="global::Acme.Models.Account">See more about the Account model</see>
		/// </remarks>
		/// </summary>
		[DetailProperty(nameof(global::Acme.Models.Account.OwnerId))]
		public virtual List<Account> CollectionOfAccountByOwner { get; set; }

		/// <summary>
		/// Collection of Account by column PrimaryContactId
		/// <remarks>
		/// <see cref="global::Acme.Models.Account">See more about the Account model</see>
		/// </remarks>
		/// </summary>
		[DetailProperty(nameof(global::Acme.Models.Account.PrimaryContactId))]
		public virtual List<Account> CollectionOfAccountByPrimaryContact { get; set; }

		/// <summary>
		/// Collection of Contact by column CreatedById
		/// <remarks>
		/// <see cref="global::Acme.Models.Contact">See more about the Contact model</see>
		/// </remarks>
		/// </summary>
		[DetailProperty(nameof(global::Acme.Models.Contact.CreatedById))]
		public virtual List<Contact> CollectionOfContactByCreatedBy { get; set; }

		/// <summary>
		/// Collection of Contact by column OwnerId
		/// <remarks>
		/// <see cref="global::Acme.Models.Contact">See more about the Contact model</see>
		/// </remarks>
		/// </summary>
		[DetailProperty(nameof(global::Acme.Models.Contact.OwnerId))]
		public virtual List<Contact> CollectionOfContactByOwner { get; set; }

		/// <summary>
		/// Collection of ContactCommunication by column ContactId
		/// <remarks>
		/// <see cref="global::Acme.Models.ContactCommunication">See more about the ContactCommunication model</see>
		/// </remarks>
		/// </summary>
		[DetailProperty(nameof(global::Acme.Models.ContactCommunication.ContactId))]
		public virtual List<ContactCommunication> CollectionOfContactCommunicationByContact { get; set; }

		#endregion
	}
}
#pragma warning restore CS8618 // Non-nullable field is uninitialized.
